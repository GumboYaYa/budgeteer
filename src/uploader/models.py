from django.db import models


class Account(models.Model):
    iban = models.CharField(max_length=22, unique=True)


class Bic(models.Model):
    bic = models.CharField(max_length=11, unique=True)


class Currency(models.Model):
    currency_short = models.CharField(max_length=3, unique=True)
    currency_long = models.CharField(max_length=100)


class Optionee(models.Model):
    name = models.CharField(max_length=200)
    iban = models.ForeignKey(Account, on_delete=models.CASCADE)
    bic = models.ForeignKey(Bic, on_delete=models.CASCADE)


class Transaction(models.Model):
    account = models.ForeignKey(Account, on_delete=models.CASCADE)
    date_booking = models.DateField()
    date_imported = models.DateField(auto_now_add=True)
    optionee = models.ForeignKey(Optionee, on_delete=models.CASCADE)
    figure = models.DecimalField(max_digits=18, decimal_places=2)
    currency = models.ForeignKey(Currency, on_delete=models.CASCADE)
    # reference = models.CharField(max_length=378)
    reference = models.CharField(max_length=500)
