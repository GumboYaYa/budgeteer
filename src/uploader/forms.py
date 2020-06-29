from django import forms
from .models import Iban, Bic, Currency, Optionee, Transaction


class FileUploadForm(forms.Form):

    file = forms.FileField(required=False)
    iban = forms.CharField(max_length=24, strip=True)
    currency = forms.CharField(max_length=3, strip=True)
    optionee_name = forms.CharField(max_length=200, strip=True)
    optionee_iban = forms.CharField(max_length=22, strip=True)
    optionee_bic = forms.CharField(max_length=11, strip=True)
    date_booking = forms.DateField()
    figure = forms.DecimalField(max_digits=18, decimal_places=2)
    reference = forms.CharField(max_length=500, strip=True)

    def save(self):
        data = self.cleaned_data
        # data = self

        iban, created = Iban.objects.get_or_create(iban=data["iban"])
        if created:
            iban.save()

        o_iban, created = Iban.objects.get_or_create(iban=data["optionee_iban"])
        if created:
            o_iban.save()

        bic, created = Bic.objects.get_or_create(bic=data["optionee_bic"])
        if created:
            bic.save()

        curr, created = Currency.objects.get_or_create(currency_short=data["currency"])
        if created:
            curr.save()

        opt, created = Optionee.objects.get_or_create(name=data["optionee_name"], iban=o_iban, bic=bic)
        if created:
            curr.save()

        transaction, created = Transaction.objects.get_or_create(
            account=iban,
            date_booking=data["date_booking"],
            optionee=opt,
            figure=data["figure"],
            currency=curr,
            reference=data["reference"],
        )
        if created:
            transaction.save()
